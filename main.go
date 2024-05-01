package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"time"
)

type Command struct {
	Cmd       string `json:"cmd"`
	Frequency int    `json:"frequency"`
}

func main() {
	// Read JSON file
	jsonFile, err := ioutil.ReadFile("command.json")
	if err != nil {
		log.Fatal("Error reading JSON file:", err)
	}

	// Parse JSON data
	var cmd Command
	err = json.Unmarshal(jsonFile, &cmd)
	if err != nil {
		log.Fatal("Error parsing JSON:", err)
	}

	ticker := time.NewTicker(time.Duration(cmd.Frequency) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Execute command
			output, err := executeCommand(cmd.Cmd)
			if err != nil {
				log.Println("Error executing command:", err)
			} else {
				// Print output to console
				fmt.Println(output)
			}
		}
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

