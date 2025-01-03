import sys
import requests
from datetime import datetime
import clickhouse_connect
from db_connections import CHCON

# https://www.mediawiki.org/wiki/API:Siteinfo
language = 'en'
base_url = f"https://{language}.wikipedia.org/w/api.php"
params = {
    'action': 'query',
    'meta': 'siteinfo',
    'siprop': 'statistics',
    'format': 'json'
}

CREATE_WIKI_STATS = """
    CREATE TABLE IF NOT EXISTS wiki_stats (
        dt DateTime,
        language String,
        pages Int32,
        articles Int32,
        edit Int32,
        images Int32,
        users Int32,
        activeusers Int32,
        admins Int32,
        jobs Int32
    ) ENGINE = MergeTree()
    ORDER BY dt
"""

DELETE_FROM_WIKI_STATS = """
    DELETE FROM wiki_stats
    WHERE
        language = '{language}'
        and dt = '{dt}'
"""

def main():
    try:
        response = requests.get(base_url, params=params)
        response.raise_for_status()
        data = response.json()
    except Exception as e:
        print(f"Error fetching data: {e}")
        sys.exit(1)

    print('Statistics:')
    print(data)

    # todo: simplify
    dt = datetime.now()
    s = data['query']['statistics']
    wiki_stats = [
        (dt, language, 
        s['pages'], s['articles'], s['edits'],
        s['images'], s['users'], s['activeusers'],
        s['admins'], s['jobs'])
    ]
    try:
        client = clickhouse_connect.get_client(**CHCON)
        client.command(CREATE_WIKI_STATS)
        client.command(DELETE_FROM_WIKI_STATS.format(language=language, dt=dt.strftime('%Y-%m-%d %H:%M:%S')))
        client.insert(table="wiki_stats", data=wiki_stats)
    except Exception as e:
        print(f"Error writing to DB: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
