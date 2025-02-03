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
    df = client.query_df("SELECT * FROM repeater.wiki_stats")
    col1, col2, col3 = st.columns(3)
    with col1:
        st.line_chart(df, x="dt", y="articles")#, title="Articles")
    with col2:
        st.line_chart(df, x="dt", y="edit")#, title="Edits")
    with col3:
        st.line_chart(df, x="dt", y="activeusers")#, title="Active Users")
elif sidebar_radio == "Linux Github Stats":
    st.header("Linux Github Stats")
    df = client.query_df("SELECT * FROM repeater.github_linux_commits_count")
    st.line_chart(df, x="dt", y="commits")#, title="Commits")
    df = client.query_df("SELECT * FROM repeater.github_linux_stats")
    col1, col2 = st.columns(2)
    with col1:
        st.line_chart(df, x="dt", y="stars")#, title="stars")
    with col2:
        st.line_chart(df, x="dt", y="size_kb")#, title="size_kb")
elif sidebar_radio == "Bitcoin Exchange Rate":
    st.header("Bitcoin Exchange Rate")
    df = client.query_df("SELECT * FROM repeater.btc_exchange_rate")
    st.line_chart(df, x="dt", y="price_usd")#, title="BTC, $")
