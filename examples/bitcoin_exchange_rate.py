import sys
import requests
import argparse
from datetime import datetime, timedelta
import clickhouse_connect
from db_connections import CHCON

# https://www.coinlore.com/cryptocurrency-data-api

# id=90 - bitcoin
source = 'coinlore'
base_url = "https://api.coinlore.net/api/ticker/?id=90"

CREATE_BTC_EXCHANGE_RATE = """
    CREATE TABLE IF NOT EXISTS btc_exchange_rate (
        dt DateTime,
        source String,
        price_usd Float32
    ) ENGINE = MergeTree()
    ORDER BY dt
"""

DELETE_FROM_BTC_EXCHANGE_RATE = """
    DELETE FROM btc_exchange_rate
    WHERE
        source = '{source}'
        and dt = '{dt}'
"""

def main():
    try:
        res = requests.get(base_url)
        res.raise_for_status()
        data = res.json()
    except Exception as e:
        print(f"Error fetching data: {e}")
        sys.exit(1)

    print('BTC Ticker:')
    print(data)
    dt = datetime.now()
    btc_exchange_rate = [(dt, source, data[0]['price_usd'])]

    try:
        client = clickhouse_connect.get_client(**CHCON)
        client.command(CREATE_BTC_EXCHANGE_RATE)
        client.command(DELETE_FROM_BTC_EXCHANGE_RATE.format(source=source, dt=dt.strftime('%Y-%m-%d %H:%M:%S')))
        client.insert(table="btc_exchange_rate", data=btc_exchange_rate)
    except Exception as e:
        print(f"Error writing to DB: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
