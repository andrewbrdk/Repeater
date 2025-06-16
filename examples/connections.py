CHCON = {
    #'host': 'localhost', # locally
    'host': 'clickhouse', # docker
    'port': 8123,
    'database': 'repeater',
    'username': 'chuser', 
    'password': 'password123'
}
# todo: same hostname in docker and host
# https://stackoverflow.com/questions/47316025/docker-compose-how-to-reference-other-service-as-localhost
# https://stackoverflow.com/questions/36151981/local-hostnames-for-docker-containers
# https://stackoverflow.com/questions/43547795/how-to-share-localhost-between-two-different-docker-containers/43554707#43554707

SMTP = {
    'server': "",
    'port': 587,
    'username': "",
    'password': "",
    'email_from': "",
    'timeout_seconds': 10
}

SLACK = {
    'webhook': ""
}