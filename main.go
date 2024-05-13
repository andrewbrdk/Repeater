package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"
const scanTasksSchedule = "*/10 * * * * *"

const webTasksList = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Tasks</title>
</head>
<body>
    <h1>Tasks</h1>
    {{range .Tasks}}
	<div>
	<details open>
	<summary><strong>{{.Title}}</strong> {{.Cron}}
	<button onclick="toggleState('{{.Title}}')">{{if .OnOff}}Turn Off{{else}}Turn On{{end}}</button>
	</summary>
	{{.HTMLTableString }}
	<!-->	<!-->
	<table>
        <tr>
            <th> Start </th>
			<th> {{.Title}} </th>
			{{range .Tasks}}
			<th> ... </th>
			<!--> <th> {{.Name}} </th> <!-->
			{{end}}
		</tr>
		{{range .History}}
			<tr>
                <td>{{.StartTime.Format "2006-01-02 15:04:05"}}</td>
				<td>{{.Status.HTMLStatusString}}</td>
				{{range .Details}}
					<td>{{.Status.HTMLStatusString}} </td>
				{{end}}
			</tr>
		{{end}}
    </table>
	<!-->	<!-->
	</details>
	</div>
    {{end}}
	<script>
        function toggleState(title) {
            fetch('/toggle-state?title=' + title)
				.then(response => {
					location.reload();
				})
                .catch(error => {
                    console.error('Error toggling state:', error);
                });
        }
    </script>
</body>
</html>
`

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
)

func (s RunStatus) String() string {
	switch s {
	case RunSuccess:
		return "success"
	case RunFailure:
		return "failure"
	default:
		return "unknown"
	}
}

func (s RunStatus) HTMLStatusString() template.HTML {
	switch s {
	case RunSuccess:
		//return "s"
		//return "⬛"
		//return "&#9632;"
		return "■"
	case RunFailure:
		//return "f"
		return "⨯"
		//return "&#9949;"
		//return "&#x2A2F;"
	default:
		return "?"
	}
}

type Task struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type TaskRun struct {
	Name      string
	Cmd       string
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
}

type TasksSequenceRun struct {
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
	Details   []*TaskRun
}

type TasksSequence struct {
	File        string
	MD5         [16]byte
	Title       string  `json:"title"`
	Cron        string  `json:"cron"`
	Tasks       []*Task `json:"tasks"`
	cronID      cron.EntryID
	cronJobFunc cron.FuncJob
	History     []*TasksSequenceRun
	OnOff       bool
}

type AMessOfTasks struct {
	Tasks []*TasksSequence
}

func main() {
	var tasks AMessOfTasks
	c := cron.New(cron.WithSeconds())
	c.Start()
	dirScanCronJobFunc := func() { scanAndScheduleTasks(&tasks, c) }
	c.AddFunc(scanTasksSchedule, dirScanCronJobFunc)
	webServer(&tasks)
}

func webServer(tasks *AMessOfTasks) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		listTasks(w, tasks)
	})
	http.HandleFunc("/toggle-state", func(w http.ResponseWriter, r *http.Request) {
		toggleStateHandler(w, r, tasks)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func listTasks(w http.ResponseWriter, tasks *AMessOfTasks) {
	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))
	err := tmpl.Execute(w, tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (tseq TasksSequence) HTMLTableString() template.HTML {
	var sb strings.Builder
	sb.WriteString("<table>\n")
	for r := -1; r < len(tseq.Tasks); r++ {
		sb.WriteString("<tr>\n")
		for c := -1; c <= len(tseq.History); c++ {
			if r == -1 && c == -1 {
				sb.WriteString("<th> </th>")
			} else if r == -1 && c < len(tseq.History) {
				sb.WriteString(fmt.Sprintf("<th> %s </th>", tseq.History[c].Status.HTMLStatusString()))
			} else if r == -1 && c == len(tseq.History) {
				//todo: newest is leftmost, reorder history
				sb.WriteString("<th>&#9633;</th>")
			} else if c == -1 {
				//todo: escape task names
				sb.WriteString(fmt.Sprintf("<td> %s </td>", tseq.Tasks[r].Name))
			} else if c < len(tseq.History) {
				sb.WriteString(fmt.Sprintf("<td> %s </td>", tseq.History[c].Details[r].Status.HTMLStatusString()))
			} else if c == len(tseq.History) {
				//todo: newest is leftmost, reorder history
				sb.WriteString("<td>&#9633;</td>")
			} else {
				slog.Error("this is not supposed to happen")
			}
		}
		sb.WriteString("</tr>\n")
	}
	sb.WriteString("</table>\n")
	return template.HTML(sb.String())
}

func toggleStateHandler(w http.ResponseWriter, r *http.Request, tasks *AMessOfTasks) {
	title := r.FormValue("title")

	for _, taskSeq := range tasks.Tasks {
		if taskSeq.Title == title {
			taskSeq.OnOff = !taskSeq.OnOff
			slog.Infof("Toggled state of %s to %v", title, taskSeq.OnOff)
			return
		}
	}
	http.Error(w, "TasksSequence not found", http.StatusNotFound)
}

func scanAndScheduleTasks(tasks *AMessOfTasks, c *cron.Cron) {
	dir := tasksDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Errorf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".tasks" {
			tseq, err := processJSONFile(path)
			if err != nil {
				return err
			}
			addAndScheduleTasks(tseq, tasks, c)
		}
		return nil
	})
	if err != nil {
		slog.Fatal(err)
	}
}

func processJSONFile(filePath string) (*TasksSequence, error) {
	var tseq TasksSequence
	tseq.File = filePath
	tseq.OnOff = true
	jsonFile, err := os.ReadFile(filePath)
	if err != nil {
		slog.Errorf("Error reading JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	tseq.MD5 = md5.Sum(jsonFile)
	err = json.Unmarshal(jsonFile, &tseq)
	if err != nil {
		slog.Errorf("Error parsing JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	return &tseq, nil
}

func addAndScheduleTasks(tseq *TasksSequence, tasks *AMessOfTasks, c *cron.Cron) {
	for _, existing := range tasks.Tasks {
		if existing.File == tseq.File && existing.MD5 == tseq.MD5 {
			slog.Infof("File '%s' already loaded, skipping.", tseq.File)
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, tseq)
	slog.Infof("Added TasksSequence '%s' from file '%s'", tseq.Title, tseq.File)
	tseq.cronJobFunc = func() { runTaskCommands(tseq) }
	tseq.cronID, _ = c.AddFunc(tseq.Cron, tseq.cronJobFunc)
}

func runTaskCommands(tseq *TasksSequence) {
	if !tseq.OnOff {
		slog.Infof("Skipping '%s'", tseq.Title)
		return
	}
	slog.Infof("Running '%s'", tseq.Title)

	run := &TasksSequenceRun{StartTime: time.Now()}
	defer func() {
		run.EndTime = time.Now()
		//append to front to simplify web output
		tseq.History = append([]*TasksSequenceRun{run}, tseq.History...)
	}()

	for _, c := range tseq.Tasks {
		cmdStartTime := time.Now()
		output, err := executeCommand(c.Cmd)
		cmdEndTime := time.Now()
		cmdStatus := RunSuccess
		if err != nil {
			slog.Errorf("Error executing '%s'-'%s': %v\n", tseq.Title, c.Name, err)
			cmdStatus = RunFailure
			run.Status = RunFailure
		}
		slog.Infof("Task '%s', command '%s', output: '%s'\n", tseq.Title, c.Name, output)
		run.Details = append(run.Details, &TaskRun{
			Name:      c.Name,
			Cmd:       c.Cmd,
			StartTime: cmdStartTime,
			EndTime:   cmdEndTime,
			Status:    cmdStatus,
		})
		if err != nil {
			//todo: skipToNextTask()
			slog.Errorf("Should be skipping to next task")
			//return
		}
	}
	run.Status = RunSuccess
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
