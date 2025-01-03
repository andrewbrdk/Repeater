import sys
import requests
import argparse
from datetime import datetime, timedelta
import clickhouse_connect
from db_connections import CHCON

# https://doc.wikimedia.org/generated-data-platform/aqs/analytics-api/concepts/page-views.html
# https://doc.wikimedia.org/generated-data-platform/aqs/analytics-api/reference/page-views.html

wikiproject = 'en.wikipedia'
base_url = "https://wikimedia.org/api/rest_v1/metrics/pageviews/aggregate/{wikiproject}/all-access/user/daily/{start_date}/{end_date}"

# https://foundation.wikimedia.org/wiki/Policy:Wikimedia_Foundation_User-Agent_Policy
headers = {'User-Agent': 'CoolBot/0.0 (https://example.org/coolbot/; coolbot@example.org)'}

CREATE_PAGEVIEWS = """
    CREATE TABLE IF NOT EXISTS pageviews (
        dt Date,
        project String,
        views Int32
    ) ENGINE = MergeTree()
    ORDER BY dt
"""

DELETE_FROM_PAGEVIEWS = """
    DELETE FROM pageviews
    WHERE
        project = '{wikiproject}'
        and dt >= '{start_date}'
        and dt <= '{end_date}'
"""

def main():
    parser = argparse.ArgumentParser(description="Get Wikipedia pageviews")
    parser.add_argument("--end_date", required=True, type=str, help="End date in YYYYMMDD")
    parser.add_argument("--start_date", type=str, help="Start date in YYYYMMDD (optional). start_date=end_date-7 if not specified.")
    args = parser.parse_args()
    
    end_date = args.end_date
    start_date = args.start_date
    if not start_date:
        start_date = (datetime.strptime(end_date, "%Y%m%d") - timedelta(days=7)).strftime('%Y%m%d')

    try:
        response = requests.get(base_url.format(wikiproject=wikiproject, start_date=start_date, end_date=end_date), 
                                headers=headers)
        response.raise_for_status()
        data = response.json()
    except Exception as e:
        print(f"Error fetching data: {e}")
        sys.exit(1)

    views = [
        (datetime.strptime(item['timestamp'],'%Y%m%d00').date(), item['project'], item['views']) 
        for item in data['items']
    ]

    print('Pageviews:')
    print(views)

    try:
        client = clickhouse_connect.get_client(**CHCON)
        client.command(CREATE_PAGEVIEWS)
        client.command(DELETE_FROM_PAGEVIEWS.format(wikiproject=wikiproject, start_date=start_date, end_date=end_date))
        client.insert(table="pageviews", data=views)
    except Exception as e:
        print(f"Error writing to DB: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
