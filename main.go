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

type CommandList struct {
	Commands []*Command
	Mutex    sync.Mutex
}

func main() {
	var commandList CommandList

	// Start a goroutine for file scanning
	go func() {
		for {
			scanFiles(".", &commandList)
			time.Sleep(10 * time.Second) // Scan directory every 10 seconds
		}
	}()

	// Start a goroutine for executing commands
	go runCommands(&commandList)

	// Keep the main goroutine running
	select {}
}

func scanFiles(dir string, commandList *CommandList) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".json" {
			// Process JSON file
			processJSONFile(path, commandList)
		}
		return nil
	})
}

func processJSONFile(filePath string, commandList *CommandList) {
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

	commandList.Mutex.Lock()
	defer commandList.Mutex.Unlock()

	// Check if the command with the same title already exists
	for _, existingCmd := range commandList.Commands {
		if existingCmd.Title == cmd.Title {
			// If a command with the same title already exists, skip processing
			log.Printf("Command with title '%s' already exists, skipping processing for JSON file %s\n", cmd.Title, filePath)
			return
		}
	}

	// Add command to the command list
	commandList.Commands = append(commandList.Commands, &cmd)

	fmt.Printf("Populated command list with command from JSON file %s. Title: %s, Command: %s, Frequency: %d seconds\n", filePath, cmd.Title, cmd.Cmd, cmd.Frequency)
}

func runCommands(commandList *CommandList) {
	for {
		time.Sleep(1 * time.Second) // Check commands every second

		commandList.Mutex.Lock()

		for _, cmd := range commandList.Commands {
			go func(cmd *Command) {
				// Configure execution for the command
				ticker := time.NewTicker(time.Duration(cmd.Frequency) * time.Second)
				defer ticker.Stop()

				for range ticker.C {
					// Execute command
					output, err := executeCommand(cmd.Cmd)
					if err != nil {
						log.Printf("Error executing command with title '%s': %v\n", cmd.Title, err)
					} else {
						// Print output to console
						fmt.Printf("Output of command with title '%s': %s\n", cmd.Title, output)
					}
				}
			}(cmd)
		}

		commandList.Mutex.Unlock()
	}
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

