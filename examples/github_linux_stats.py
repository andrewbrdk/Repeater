import sys
import requests
from datetime import datetime
import clickhouse_connect
from db_connections import CHCON

OWNER = 'torvalds'
REPO = 'linux'
GH_REPO = f"https://api.github.com/repos/{OWNER}/{REPO}"

CREATE_GITHUB_LINUX_STATS = """
    CREATE TABLE IF NOT EXISTS github_linux_stats (
        dt DateTime,
        size_kb Int32,
        stars Int32,
        forks Int32
    ) ENGINE = MergeTree()
    ORDER BY dt
"""

DELETE_FROM_GITHUB_LINUX_STATS = """
    DELETE FROM github_linux_stats
    WHERE
        dt = '{dt}'
"""

def main():
    try:
        res = requests.get(GH_REPO)
        res.raise_for_status()
        data = res.json()
    except Exception as e:
        print(f"Error fetching data: {e}")
        sys.exit(1)

    dt = datetime.now()
    linux_stats = [(dt, data['size'], data['stargazers_count'], data['forks'])]
    print('linux stats:')
    print(linux_stats)

    try:
        client = clickhouse_connect.get_client(**CHCON)
        client.command(CREATE_GITHUB_LINUX_STATS)
        client.command(DELETE_FROM_GITHUB_LINUX_STATS.format(dt=dt.strftime('%Y-%m-%d %H:%M:%S')))
        client.insert(table="github_linux_stats", data=linux_stats)
    except Exception as e:
        print(f"Error writing to DB: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
