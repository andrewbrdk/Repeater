package main

import (
    "encoding/json"
    "fmt"
    "io/ioutil"
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
        fmt.Println("Error reading JSON file:", err)
        return
    }

    // Parse JSON data
    var cmd Command
    err = json.Unmarshal(jsonFile, &cmd)
    if err != nil {
        fmt.Println("Error parsing JSON:", err)
        return
    }

    ticker := time.NewTicker(time.Duration(cmd.Frequency) * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            // Execute command
            executeCommand(cmd.Cmd)
        }
    }
}

func executeCommand(command string) {
    cmd := exec.Command("/bin/bash", "-c", command)
    err := cmd.Run()
    if err != nil {
        fmt.Println("Error executing command:", err)
        return
    }
}

