package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slices"

	graphviz "github.com/awalterschulze/gographviz"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/mod/modfile"
)

func main() {
	cmd := exec.Command("dot", "-V")
	if err := cmd.Run(); err != nil {
		log.Fatalf("Graphviz is not installed: %v", err)
	}

	if len(os.Args) < 2 {
		log.Fatal("Please provide a directory path")
	}

	url, err := convertHTTPtoSSH(os.Args[1])
	if err != nil {
		log.Fatalf("Please provide valid https:// link to your github profile: %s", err.Error())
	}

	pemFile, err := os.Open(filepath.Join(os.Getenv("HOME"), "/.ssh/id_rsa"))
	if err != nil {
		log.Fatal(err)
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(pemFile); err != nil {
		log.Fatal(err)
	}

	// attempt to use systems SSH keys
	sshAuth, err := ssh.NewPublicKeys("git", buf.Bytes(), "")
	if err != nil {
		log.Fatal(err)
	}

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:           url,
		Auth:          sshAuth,
		ReferenceName: plumbing.NewBranchReferenceName("master"), // TODO: assumes main, fallback to master?
		Depth:         1,
	})
	if err != nil {
		log.Fatalf("Error cloning repository: %s", err)
	}

	tree, err := repo.Worktree()
	if err != nil {
		log.Fatal(err)
	}

	var moduleName string

	err = util.Walk(tree.Filesystem, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "go.mod") {
			file, err := tree.Filesystem.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(file); err != nil {
				return err
			}

			modFile, err := modfile.Parse(path, buf.Bytes(), nil)
			if err != nil {
				log.Fatalf("failed to parse go.mod file: %v", err)
			}

			moduleName = modFile.Module.Mod.Path
			return nil
		}

		return nil
	})

	if err != nil {
		log.Fatalf("failed to search the git work tree for the `go.mod` file: %s", err.Error())
	}

	dependancyGraph := make(map[string][]string)

	err = util.Walk(tree.Filesystem, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Ext(path) == ".go" {
			file, err := tree.Filesystem.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			name := getPackageName(moduleName, path)

			if _, ok := dependancyGraph[name]; !ok {
				dependancyGraph[name] = make([]string, 0)
			}

			source, err := parser.ParseFile(token.NewFileSet(), path, file, parser.ImportsOnly)
			if err != nil {
				log.Fatalf("Failed to parse file: %v", err)
			}

			for _, importSpec := range source.Imports {
				dependancy := importSpec.Path.Value[1 : len(importSpec.Path.Value)-1]
				if _, ok := dependancyGraph[dependancy]; !ok {
					dependancyGraph[dependancy] = make([]string, 0)
				}
				dependancyGraph[name] = append(dependancyGraph[name], dependancy)
			}
		}

		return nil
	})

	if err != nil {
		log.Fatal("failed to walk directory provided: %w", err)
	}

	for _, deps := range dependancyGraph {
		slices.Sort(deps)
		slices.Compact(deps)
	}

	graph := graphviz.NewGraph()
	graph.Directed = true

	seen := make(map[string]bool)

	for module, dependancies := range dependancyGraph {
		if !seen[module] {
			seen[module] = true
			if err := graph.AddNode("G", fmt.Sprintf("%q", module), nil); err != nil {
				panic(err)
			}
		}

		for _, dependancy := range dependancies {
			if !seen[dependancy] {
				seen[dependancy] = true
				if err := graph.AddNode("G", fmt.Sprintf("%q", dependancy), nil); err != nil {
					panic(err)
				}
			}

			if err := graph.AddEdge(fmt.Sprintf("%q", dependancy), fmt.Sprintf("%q", module), true, nil); err != nil {
				panic(err)
			}
		}
	}

	if err := os.WriteFile("graph.dot", []byte(graph.String()), fs.ModePerm); err != nil {
		log.Fatal("couldn't write graph.dot file")
	}

	cmd = exec.Command("dot", "-Tpdf", "graph.dot", "-o", "graph.pdf")
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}

func convertHTTPtoSSH(httpURL string) (string, error) {
	if !strings.HasPrefix(httpURL, "https://") {
		return "", fmt.Errorf("invalid URL format")
	}

	sshURL := strings.Replace(httpURL, "https://", "git@", 1)
	sshURL = strings.Replace(sshURL, "/", ":", 1)

	return sshURL, nil
}

func getPackageName(modulePath, relativeFilePath string) string {
	// Join the module path and the directory of the relative file path
	fullPath := filepath.Join(modulePath, filepath.Dir(relativeFilePath))
	return fullPath
}
