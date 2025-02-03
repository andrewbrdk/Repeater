import pandas as pd
import clickhouse_connect
import streamlit as st
from db_connections import CHCON

client = clickhouse_connect.get_client(**CHCON)

sidebar_radio = st.sidebar.radio(
    "Examples",
    ("Wiki Stats", "Linux Github Stats", "Bitcoin Exchange Rate")
)

if sidebar_radio == "Wiki Stats":
    st.header("Wiki Stats")
    df = client.query_df("SELECT * FROM repeater.wiki_pageviews")
    st.line_chart(df, x="dt", y="views")#, title="Views")
elif sidebar_radio == "Linux Github Stats":
    st.header("Linux Github Stats")
    df = client.query_df("SELECT * FROM repeater.github_linux_commits_count")
    st.line_chart(df, x="dt", y="commits")#, title="Commits")
elif sidebar_radio == "Bitcoin Exchange Rate":
    st.header("Bitcoin Exchange Rate")
    df = client.query_df("SELECT * FROM repeater.btc_exchange_rate")
    st.line_chart(df, x="dt", y="price_usd")#, title="BTC, $")
