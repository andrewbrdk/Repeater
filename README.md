### Repeater

A task scheduler for data analytics inspired by [Apache Airflow](https://airflow.apache.org/).

<p align="center">
    <a href="https://github.com/andrewbrdk/Repeater">
    <img src="https://i.ibb.co/T8XDLsP/repeater-01.png" alt="repeater-01" width="600">
    </a>
</p>

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
# Notification script parameters
REPEATER_SLACK_WEBHOOK=""
REPEATER_SMTP_SERVER=""
REPEATER_SMTP_PORT=""
REPEATER_SMTP_USER=""
REPEATER_SMTP_PASS=""
REPEATER_EMAIL_FROM=""
```

Task `cmd` [parameters](https://github.com/andrewbrdk/Repeater/blob/main/examples/templated_args.job):
```
{{.title}} - job title
{{.scheduled_dt}} - current run scheduled date in YYYY-MM-DD
```
