package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Command struct {
	Title     string `json:"title"`
	Cmd       string `json:"cmd"`
	Frequency int    `json:"frequency"`
}

func main() {
	// Regularly scan the current directory for JSON files
	for {
		filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Printf("Error accessing path %s: %v\n", path, err)
				return err
			}
			if !info.IsDir() && filepath.Ext(path) == ".json" {
				// Process JSON file
				go processJSONFile(path)
			}
			return nil
		})
		time.Sleep(10 * time.Second) // Scan directory every 10 seconds
	}
}

var (
	commandMap   = make(map[string]*sync.WaitGroup)
	commandMapMu sync.Mutex
)

func processJSONFile(filePath string) {
	// Read JSON file
	jsonFile, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Printf("Error reading JSON file %s: %v\n", filePath, err)
		return
	}

	// Parse JSON data
	var cmd Command
	err = json.Unmarshal(jsonFile, &cmd)
	if err != nil {
		log.Printf("Error parsing JSON file %s: %v\n", filePath, err)
		return
	}

	// Configure execution for valid JSON file
	ticker := time.NewTicker(time.Duration(cmd.Frequency) * time.Second)
	defer ticker.Stop()

	fmt.Printf("Configured execution for JSON file %s. Title: %s, Command: %s, Frequency: %d seconds\n", filePath, cmd.Title, cmd.Cmd, cmd.Frequency)

	commandMapMu.Lock()
	if _, ok := commandMap[cmd.Title]; !ok {
		// Start a new goroutine only if there is no existing goroutine for the title
		wg := &sync.WaitGroup{}
		commandMap[cmd.Title] = wg
		wg.Add(1)

		go func(cmd Command) {
			defer wg.Done()
			for range ticker.C {
				// Execute command
				output, err := executeCommand(cmd.Cmd)
				if err != nil {
					log.Printf("Error executing command from JSON file %s: %v\n", filePath, err)
				} else {
					// Print output to console
					fmt.Printf("Output of command '%s' from JSON file %s: %s\n", cmd.Title, filePath, output)
				}
			}
		}(cmd)
	}
	commandMapMu.Unlock()
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

