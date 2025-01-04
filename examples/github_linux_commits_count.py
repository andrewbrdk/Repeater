import sys
import requests
from datetime import datetime, timezone, timedelta
import clickhouse_connect
from db_connections import CHCON

OWNER = 'torvalds'
REPO = 'linux'
GH_COMACT = f"https://api.github.com/repos/{OWNER}/{REPO}/stats/commit_activity"

CREATE_GITHUB_LINUX_COMMITS_COUNT = """
    CREATE TABLE IF NOT EXISTS github_linux_commits_count (
        dt Date,
        commits Int32
    ) ENGINE = MergeTree()
    ORDER BY dt
"""

DELETE_FROM_GITHUB_LINUX_COMMITS_COUNT = """
    DELETE FROM github_linux_commits_count
    WHERE
        dt >= '{dt_start}'
        and dt <= '{dt_end}'
"""

def main():
    try:
        res = requests.get(GH_COMACT)
        res.raise_for_status()
        data = res.json()
    except Exception as e:
        print(f"Error fetching data: {e}")
        sys.exit(1)

    commits_count = []
    for week in data:
        dt = datetime.fromtimestamp(week['week'], timezone.utc).date()
        for daynum, commits in enumerate(week['days']):
            commits_count.append([dt + timedelta(days=daynum), commits])
    dt_start = commits_count[0][0].strftime('%Y-%m-%d')
    dt_end = commits_count[-1][0].strftime('%Y-%m-%d')
    print('The first and the last day of the fetched commits activity:') 
    print(commits_count[0], commits_count[-1])

    try:
        client = clickhouse_connect.get_client(**CHCON)
        client.command(CREATE_GITHUB_LINUX_COMMITS_COUNT)
        client.command(DELETE_FROM_GITHUB_LINUX_COMMITS_COUNT.format(dt_start=dt_start, dt_end=dt_end))
        client.insert(table="github_linux_commits_count", data=commits_count)
    except Exception as e:
        print(f"Error writing to DB: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
