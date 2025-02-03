### Repeater

Task scheduler for data analytics

<p align="center">
    <a href="https://github.com/andrewbrdk/Repeater">
    <img src="https://i.ibb.co/T8XDLsP/repeater-01.png" alt="repeater-01" width="600">
    </a>
</p>

The service should be available at [http://localhost:8080](http://localhost:8080) after the following commands:

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
REPEATER_PORT=":8080"                   # web server port  
REPEATER_PASSWORD=""                    # web auth password
REPEATER_JOBS_DIRECTORY="./examples/"   # jobs directory
```

Task `cmd` [parameters](https://github.com/andrewbrdk/Repeater/blob/main/examples/template.job):
```
{{.title}} - job title
{{.scheduled_dt}} - current run scheduled date in YYYY-MM-DD
```

Repeater is inspired by [Apache Airflow](https://airflow.apache.org/). Key differences are: 

* No DAGs. Only sequences of commands.
* No operators. Only command line programs. 
* No python code for DAGs definitions. Tasks are defined in config files.
* No user accounts. Only a single user.
