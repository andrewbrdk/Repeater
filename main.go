package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type Task struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type DAG struct {
	Title     string  `json:"title"`
	Frequency int     `json:"frequency"`
	Tasks     []*Task `json:"tasks"`
}

type DAGList struct {
	DAGS []*DAG
}

func main() {
	var dags DAGList
	scanDAGSDir(&dags)
	runDAGS(&dags)
}

func scanDAGSDir(dags *DAGList) {
	dir := "./"
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".dag" {
			dag, err := processJSONFile(path)
			if err != nil {
				return err
			}
			addNewDAG(path, dag, dags)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

func processJSONFile(filePath string) (*DAG, error) {
	var dag DAG
	jsonFile, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Printf("Error reading JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	err = json.Unmarshal(jsonFile, &dag)
	if err != nil {
		log.Printf("Error parsing JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	return &dag, nil
}

func addNewDAG(path string, dag *DAG, dags *DAGList) {
	for _, existing := range dags.DAGS {
		if existing.Title == dag.Title {
			log.Printf("DAG with title '%s' already exists, skipping processing.\n", dag.Title)
			return
		}
	}
	dags.DAGS = append(dags.DAGS, dag)
	fmt.Printf("Added DAG '%s' from file %s.\n", dag.Title, path)
}

func runDAGS(dags *DAGList) {
	for _, d := range dags.DAGS {
		runDAGTasks(d)
	}
}

func runDAGTasks(dag *DAG) {
	for _, t := range dag.Tasks {
		output, err := executeCommand(t.Cmd)
		if err != nil {
			log.Printf("Error executing command in DAG '%s', task '%s': %v\n", dag.Title, t.Name, err)
		} else {
			fmt.Printf("DAG '%s', task '%s', output: %s\n", dag.Title, t.Name, output)
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
