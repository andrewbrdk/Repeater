import pandas as pd
import clickhouse_connect
import streamlit as st
#import altair as alt
import plotly.graph_objects as go
from db_connections import CHCON

client = clickhouse_connect.get_client(**CHCON)

sidebar_radio = st.sidebar.radio(
    "Examples",
    ("Wiki Stats", "Linux Github Stats", "Bitcoin Exchange Rate")
)

if sidebar_radio == "Wiki Stats":
    st.header("Wiki Stats")
    try:
        df = client.query_df("SELECT dt, project, views FROM repeater.wiki_pageviews")
    except:
        df = None
        st.text("Can't read data for the 'Pageviews' plot. Make sure to run 'wiki' job at least once.")
    if df is not None:
        fig = go.Figure()
        fig.add_trace(go.Scatter(x=df['dt'], y=df['views'], line_color='black'))
        fig.update_layout(title='Pageviews', hovermode="x",
                          yaxis_range=[0, max(df['views'])*1.1])
        st.plotly_chart(fig)
    try:
        df = client.query_df("SELECT * FROM repeater.wiki_stats")
        col1, col2, col3 = st.columns(3)
        with col1:
            st.line_chart(df, x="dt", y="articles")#, title="Articles")
        with col2:
            st.line_chart(df, x="dt", y="edit")#, title="Edits")
        with col3:
            st.line_chart(df, x="dt", y="activeusers")#, title="Active Users")
    except:
        st.text("Error displaying plot. Run wiki_stats.py script at least once.")
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
    try:
        df = client.query_df("SELECT dt, price_usd FROM repeater.btc_exchange_rate")
    except:
        df = None
        st.text("Can't read data for the 'BTC, $' plot. Make sure to run 'bitcoin' job at least once.")
    if df is not None:
        st.line_chart(df, x="dt", y="price_usd")#, title="BTC, $")
