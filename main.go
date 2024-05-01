package main

import (
    "os/exec"
    "time"
)

func main() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            cmd := exec.Command("/bin/bash", "-c", "echo 'Hello World'")
            cmd.Run()
        }
    }
}

