package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/robfig/cron/v3"
)

const Port = ":8080"
const DagDir = "./"

type Task struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type Run struct {
	StartTime time.Time
	EndTime   time.Time
	Status    string
}

type DAG struct {
	Title       string  `json:"title"`
	Cron        string  `json:"cron"`
	Tasks       []*Task `json:"tasks"`
	cronID      cron.EntryID
	cronJobFunc cron.FuncJob
	History     []*Run
}

type DAGList struct {
	DAGs []*DAG
}

func main() {
	var dags DAGList
	c := cron.New(cron.WithSeconds())
	c.Start()
	scanDAGsDir(&dags)
	runDAGs(&dags, c)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		listDAGs(w, dags, c)
	})

	log.Fatal(http.ListenAndServe(Port, nil))
}

func listDAGs(w http.ResponseWriter, dags DAGList, c *cron.Cron) {
	tmpl := template.Must(template.ParseFiles("dag_list.html"))

	type TemplateData struct {
		DAGs []*DAG
	}

	data := TemplateData{DAGs: dags.DAGs}

	err := tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func scanDAGsDir(dags *DAGList) {
	dir := DagDir
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
	jsonFile, err := os.ReadFile(filePath)
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
	for _, existing := range dags.DAGs {
		if existing.Title == dag.Title {
			log.Printf("DAG with title '%s' already exists, skipping processing.\n", dag.Title)
			return
		}
	}
	dags.DAGs = append(dags.DAGs, dag)
	fmt.Printf("Added DAG '%s' from file %s.\n", dag.Title, path)
}

func runDAGs(dags *DAGList, c *cron.Cron) {
	for _, d := range dags.DAGs {
		d := d // Capture d variable
		d.cronJobFunc = func() { runDAGTasks(d) }
		d.cronID, _ = c.AddFunc(d.Cron, d.cronJobFunc)
	}
}

func runDAGTasks(dag *DAG) {
	fmt.Printf("Running DAG '%s'\n", dag.Title)

	run := &Run{StartTime: time.Now()}
	defer func() {
		run.EndTime = time.Now()
		dag.History = append(dag.History, run)
	}()

	for _, t := range dag.Tasks {
		output, err := executeCommand(t.Cmd)
		if err != nil {
			log.Printf("Error executing command in DAG '%s', task '%s': %v\n", dag.Title, t.Name, err)
			run.Status = "failure"
			return
		}
		fmt.Printf("DAG '%s', task '%s', output: %s\n", dag.Title, t.Name, output)
	}
	run.Status = "success"
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
