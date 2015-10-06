// Copyright 2015 Unknwon
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package models

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Unknwon/com"
	"github.com/Unknwon/log"
	"github.com/mschoch/blackfriday-text"
	"github.com/russross/blackfriday"
	"gopkg.in/ini.v1"

	"github.com/peachdocs/peach/modules/setting"
)

type Node struct {
	Name    string // Name in TOC
	Title   string // Name in given language
	content []byte
	Text    string // Clean text without formatting

	Plain    bool   // Root node without content
	FileName string // Full path with .md extension
	Nodes    []*Node
}

var textRender = blackfridaytext.TextRenderer()

func parseNodeName(name string, data []byte) (string, []byte) {
	data = bytes.TrimSpace(data)
	if len(data) < 3 || string(data[:3]) != "---" {
		return name, []byte("")
	}
	endIdx := bytes.Index(data[3:], []byte("---")) + 3
	if endIdx == -1 {
		return name, []byte("")
	}

	opts := strings.Split(strings.TrimSpace(string(string(data[3:endIdx]))), "\n")

	title := name
	for _, opt := range opts {
		infos := strings.SplitN(opt, ":", 2)
		if len(infos) != 2 {
			continue
		}

		switch strings.TrimSpace(infos[0]) {
		case "name":
			title = strings.TrimSpace(infos[1])
		}
	}

	return title, data[endIdx+3:]
}

func (n *Node) ReloadContent() error {
	data, err := ioutil.ReadFile(n.FileName)
	if err != nil {
		return err
	}

	n.Title, data = parseNodeName(n.Name, data)
	n.Plain = len(bytes.TrimSpace(data)) == 0

	if !n.Plain {
		n.content = markdown(data)
		n.Text = string(bytes.ToLower(blackfriday.Markdown(data, textRender, 0)))
	}
	return nil
}

func (n *Node) Content() []byte {
	if !setting.ProdMode {
		if err := n.ReloadContent(); err != nil {
			log.Error("Fail to reload content: %v", err)
		}
	}

	return n.content
}

// Toc represents table of content in a specific language.
type Toc struct {
	RootPath string
	Lang     string
	Nodes    []*Node
	Pages    []*Node
}

// GetDoc should only be called by top level toc.
func (t *Toc) GetDoc(name string) (string, []byte, bool) {
	name = strings.TrimPrefix(name, "/")

	// Returns first node whatever avaiable as default.
	if len(name) == 0 {
		if len(t.Nodes) == 0 ||
			t.Nodes[0].Plain {
			return "", nil, false
		}
		return t.Nodes[0].Title, t.Nodes[0].Content(), false
	}

	infos := strings.Split(name, "/")

	// Dir node.
	if len(infos) == 1 {
		for i := range t.Nodes {
			if t.Nodes[i].Name == infos[0] {
				return t.Nodes[i].Title, t.Nodes[i].Content(), false
			}
		}
		return "", nil, false
	}

	// File node.
	for i := range t.Nodes {
		if t.Nodes[i].Name == infos[0] {
			for j := range t.Nodes[i].Nodes {
				if t.Nodes[i].Nodes[j].Name == infos[1] {
					if com.IsFile(t.Nodes[i].Nodes[j].FileName) {
						return t.Nodes[i].Nodes[j].Title, t.Nodes[i].Nodes[j].Content(), false
					}

					// If not default language, try again.
					title, content, _ := Tocs[setting.Docs.Langs[0]].GetDoc(name)
					return title, content, true
				}
			}
		}
	}

	return "", nil, false
}

type SearchResult struct {
	Title string
	Path  string
	Match string
}

func adjustRange(start, end, length int) (int, int) {
	start -= 20
	if start < 0 {
		start = 0
	}
	end += 230
	if end > length {
		end = length
	}
	return start, end
}

func (t *Toc) Search(q string) []*SearchResult {
	if len(q) == 0 {
		return nil
	}
	q = strings.ToLower(q)

	results := make([]*SearchResult, 0, 5)

	// Dir node.
	for i := range t.Nodes {
		if idx := strings.Index(t.Nodes[i].Text, q); idx > -1 {
			start, end := adjustRange(idx, idx+len(q), len(t.Nodes[i].Text))
			results = append(results, &SearchResult{
				Title: t.Nodes[i].Title,
				Path:  t.Nodes[i].Name,
				Match: t.Nodes[i].Text[start:end],
			})
		}
	}

	// File node.
	for i := range t.Nodes {
		for j := range t.Nodes[i].Nodes {
			if idx := strings.Index(t.Nodes[i].Nodes[j].Text, q); idx > -1 {
				start, end := adjustRange(idx, idx+len(q), len(t.Nodes[i].Nodes[j].Text))
				results = append(results, &SearchResult{
					Title: t.Nodes[i].Nodes[j].Title,
					Path:  path.Join(t.Nodes[i].Name, t.Nodes[i].Nodes[j].Name),
					Match: t.Nodes[i].Nodes[j].Text[start:end],
				})
			}
		}
	}

	return results
}

var (
	tocLocker = sync.RWMutex{}
	Tocs      map[string]*Toc
)

func initToc(localRoot string) (map[string]*Toc, error) {
	tocPath := path.Join(localRoot, "TOC.ini")
	if !com.IsFile(tocPath) {
		return nil, fmt.Errorf("TOC not found: %s", tocPath)
	}

	// Generate Toc.
	tocCfg, err := ini.Load(tocPath)
	if err != nil {
		return nil, fmt.Errorf("Fail to load TOC.ini: %v", err)
	}

	tocs := make(map[string]*Toc)
	for _, lang := range setting.Docs.Langs {
		toc := &Toc{
			RootPath: localRoot,
			Lang:     lang,
		}
		dirs := tocCfg.Section("").KeyStrings()
		toc.Nodes = make([]*Node, 0, len(dirs))
		for _, dir := range dirs {
			dirName := tocCfg.Section("").Key(dir).String()
			fmt.Println(dirName + "/")
			files := tocCfg.Section(dirName).KeyStrings()

			// Skip empty directory.
			if len(files) == 0 {
				continue
			}

			dirNode := &Node{
				Name:     dirName,
				FileName: path.Join(localRoot, lang, dirName, tocCfg.Section(dirName).Key(files[0]).String()) + ".md",
				Nodes:    make([]*Node, 0, len(files)-1),
			}
			toc.Nodes = append(toc.Nodes, dirNode)

			for _, file := range files[1:] {
				fileName := tocCfg.Section(dirName).Key(file).String()
				fmt.Println(strings.Repeat(" ", len(dirName))+"|__", fileName)

				node := &Node{
					Name:     fileName,
					FileName: path.Join(localRoot, lang, dirName, fileName) + ".md",
				}
				dirNode.Nodes = append(dirNode.Nodes, node)
			}
		}

		// Single pages.
		pages := tocCfg.Section("pages").KeyStrings()
		toc.Pages = make([]*Node, 0, len(pages))
		for _, page := range pages {
			pageName := tocCfg.Section("pages").Key(page).String()
			fmt.Println(pageName)

			toc.Pages = append(toc.Pages, &Node{
				Name:     pageName,
				FileName: path.Join(localRoot, lang, pageName) + ".md",
			})
		}

		tocs[lang] = toc
	}
	return tocs, nil
}

func ReloadDocs() error {
	tocLocker.Lock()
	defer tocLocker.Unlock()

	localRoot := setting.Docs.Target

	// Fetch docs from remote.
	if setting.Docs.Type == "remote" {
		localRoot = "data/docs"

		absRoot, err := filepath.Abs(localRoot)
		if err != nil {
			return fmt.Errorf("filepath.Abs: %v", err)
		}

		// Clone new or pull to update.
		if com.IsDir(absRoot) {
			stdout, stderr, err := com.ExecCmdDir(absRoot, "git", "pull")
			if err != nil {
				return fmt.Errorf("Fail to update docs from remote source(%s): %v - %s", setting.Docs.Target, err, stderr)
			}
			fmt.Println(stdout)
		} else {
			stdout, stderr, err := com.ExecCmd("git", "clone", setting.Docs.Target, absRoot)
			if err != nil {
				return fmt.Errorf("Fail to clone docs from remote source(%s): %v - %s", setting.Docs.Target, err, stderr)
			}
			fmt.Println(stdout)
		}
	}

	if !com.IsDir(localRoot) {
		return fmt.Errorf("Documentation not found: %s - %s", setting.Docs.Type, localRoot)
	}

	tocs, err := initToc(localRoot)
	if err != nil {
		return fmt.Errorf("initToc: %v", err)
	}
	initDocs(tocs, localRoot)
	Tocs = tocs
	return nil
}

func NewContext() {
	if err := ReloadDocs(); err != nil {
		log.Fatal("Fail to init docs: %v", err)
	}
}
