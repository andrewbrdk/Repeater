### Repeater

A task orchestrator for data analytics

<p align="center">
    <a href="https://github.com/andrewbrdk/Repeater">
    <img src="https://i.ibb.co/T8XDLsP/repeater-01.png" alt="repeater-01" width="600">
    </a>
</p>

Run the following commands to start the app:

```bash
git clone https://github.com/andrewbrdk/Repeater
cd Repeater
go build 
./repeater
```

The service should be available at [http://localhost:8080](http://localhost:8080).


### Airflow Comparison 

Repeater is inspired by [Apache Airflow](https://airflow.apache.org/). The intent is to make a simplified version, suitable for small data teams. Key differences are: 

* No DAGs. Only sequences of commands.
* No operators. Only command line programs. 
* No dynamic DAGs and tasks definitions. Jobs are defined in JSON files.
* No user accounts. Only a single user.

While certain functionality is deliberately omitted, other is not implemented yet ;)