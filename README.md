### Repeater

Task scheduler for data analytics

<p align="center">
    <a href="https://github.com/andrewbrdk/Repeater">
    <img src="https://i.ibb.co/T8XDLsP/repeater-01.png" alt="repeater-01" width="600">
    </a>
</p>

Run the following commands to start the app:

```bash
git clone https://github.com/andrewbrdk/Repeater
cd Repeater
go get repeater
go build 
./repeater
```

The service should be available at [http://localhost:8080](http://localhost:8080).

To run in Docker:

```bash
docker compose up --build
``` 

[Configuration](https://github.com/andrewbrdk/Repeater/blob/main/main.go#L25):
```go
const port = ":8080"                   // web server port  
const jobsDir = "./examples/"          // jobs directory
const scanSchedule = "*/10 * * * * *"  // jobs directory rescanning schedule
const htmlTitle = "Repeater"           // html body.h1 and head.title
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
