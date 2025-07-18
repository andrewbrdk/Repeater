### Repeater

A task scheduler for data analytics inspired by [Apache Airflow](https://airflow.apache.org/).

<div align="center">
    <img src="https://i.postimg.cc/dQhV5xhG/repeater2.png" alt="repeater" width="700">
</div>

The service should start at [http://localhost:8080](http://localhost:8080) after the following commands:

```bash
git clone https://github.com/andrewbrdk/Repeater
cd Repeater
go get repeater
go build 
./repeater
```

Docker-compose starts Repeater, [ClickHouse](https://clickhouse.com/), [ch-ui](https://github.com/caioricciuti/ch-ui) and [Streamlit](https://streamlit.io/) to run examples:

```bash
docker compose up --build
```
Repeater: [http://localhost:8080](http://localhost:8080),  
Streamlit: [http://localhost:8002](http://localhost:8002),  
ch-ui: [http://localhost:8001](http://localhost:8001),  
ClickHouse: [http://localhost:8123](http://localhost:8123) and [http://localhost:9000](http://localhost:9000).


Optional environmental variables:
```bash
REPEATER_PORT=":8080"                          # web server port  
REPEATER_PASSWORD=""                           # web auth password
REPEATER_JOBS_DIRECTORY="./examples/"          # jobs directory
REPEATER_NOTIFY="python3 ./examples/notify.py" # task failure notification script
REPEATER_LOGS_DIRECTORY="/tmp/repeater/"       # tasks output directory
```

Job example
```toml
title = "example"
cron = "*/10 * * * * *"            # cron schedule with seconds, optional
retries = 1                        # task retries, optional
task_timeout = 15                  # execution timeout, seconds, optional
emails = ["yourmail@example.com"]  # email on failure, optional

# Task execution order, optional.
# List of lists of task names. 
# Tasks in inner lists run in parallel. 
# Outer list order is sequential.
# If 'order' is not specified, tasks run sequentially as defined in [[tasks]].
order = [                          
    ["hello_world", "wait_5s"],
    ["echo_args", "wait_10s"]
]                                  

[[tasks]]
name = "hello_world"
cmd = "echo Hello, world"
timeout = 15                       # overrides job-level task_timeout 
retries = 2                        # overrides job-level retries
emails = ["taskmail@example.com"]  # overrides job-level emails

[[tasks]]
name = "wait_5s" 
cmd = "sleep 5 && echo 'done'"

[[tasks]]
name = "echo_args" 
cmd = "echo \"{{.title}}\" {{.scheduled_dt}}"

[[tasks]]
name = "wait_10s" 
cmd = "sleep 10 && echo 'done'"

#cmd templated args:
#{{.title}} - job title
#{{.scheduled_dt}} - current run scheduled date in YYYY-MM-DD
```
