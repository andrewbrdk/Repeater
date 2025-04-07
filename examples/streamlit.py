import pandas as pd
import clickhouse_connect
import streamlit as st
import plotly.graph_objects as go
from db_connections import CHCON

client = clickhouse_connect.get_client(**CHCON)

st.set_page_config(layout="wide")
sidebar_radio = st.sidebar.radio(
    "Examples",
    ("En.Wikipedia Stats", "Linux Github Stats", "Bitcoin Exchange Rate")
)

if sidebar_radio == "En.Wikipedia Stats":
    st.header("En.Wikipedia Stats")
    try:
        df = client.query_df("SELECT dt, project, views FROM repeater.wiki_pageviews")
    except:
        df = None
        st.text("Can't read data for the 'Pageviews' plot. Make sure to run 'wiki' job at least once.")
    if df is not None:
        fig = go.Figure()
        fig.add_trace(go.Scatter(x=df['dt'], y=df['views'], line_color='black'))
        fig.update_layout(title='Pageviews',
                          yaxis_range=[0, max(df['views'])*1.1],
                          hovermode="x",
                          height=550)
        st.plotly_chart(fig)
    try:
        df = client.query_df("SELECT dt, articles, edit, activeusers FROM repeater.wiki_stats")
    except:
        df = None
        st.text("Can't read data for the plots. Make sure to run 'wiki' job at least once.")
    if df is not None:
        col1, col2, col3 = st.columns(3)
        with col1:
            fig = go.Figure()
            fig.add_trace(go.Scatter(x=df['dt'], y=df['articles'], line_color='black'))
            fig.update_layout(title='Articles',
                              hovermode="x",
                              yaxis_range=[0, max(df['articles'])*1.1])
            st.plotly_chart(fig)
        with col2:
            fig = go.Figure()
            fig.add_trace(go.Scatter(x=df['dt'], y=df['edit'], line_color='black'))
            fig.update_layout(title='Edits',
                              hovermode="x",
                              yaxis_range=[0, max(df['edit'])*1.1])
            st.plotly_chart(fig)
        with col3:
            fig = go.Figure()
            fig.add_trace(go.Scatter(x=df['dt'], y=df['activeusers'], line_color='black'))
            fig.update_layout(title='Active Users',
                              hovermode="x",
                              yaxis_range=[0, max(df['activeusers'])*1.1])
            st.plotly_chart(fig)
elif sidebar_radio == "Linux Github Stats":
    st.header("Linux Github Stats")
    try:
        df = client.query_df("SELECT dt, commits FROM repeater.github_linux_commits_count")
    except:
        df = None
        st.text("Can't read data for the 'Github Linux Commits' plot. Make sure to run 'github_linux' job at least once.")
    if df is not None:
        fig = go.Figure()
        fig.add_trace(go.Scatter(x=df['dt'], y=df['commits'], line_color='black'))
        fig.update_layout(title='Commits',
                          yaxis_range=[0, max(df['commits'])*1.1],
                          hovermode="x",
                          height=550)
        st.plotly_chart(fig)
    try:
        df = client.query_df("SELECT dt, stars, size_kb FROM repeater.github_linux_stats")
    except:
        df = None
        st.text("Can't read data for the plots. Make sure to run 'github_linux' job at least once.")
    if df is not None:
        col1, col2 = st.columns(2)
        with col1:
            fig = go.Figure()
            fig.add_trace(go.Scatter(x=df['dt'], y=df['stars'], line_color='black'))
            fig.update_layout(title='Stars',
                            yaxis_range=[0, max(df['stars'])*1.1],
                            hovermode="x",
                            height=550)
            st.plotly_chart(fig)
        with col2:
            df['size_mb'] = df['size_kb'] / 1024
            fig = go.Figure()
            fig.add_trace(go.Scatter(x=df['dt'], y=df['size_mb'], line_color='black'))
            fig.update_layout(title='Size, MB',
                            yaxis_range=[0, max(df['size_mb'])*1.1],
                            yaxis_tickformat=".0f",
                            hovermode="x",
                            height=550)
            st.plotly_chart(fig)
elif sidebar_radio == "Bitcoin Exchange Rate":
    st.header("Bitcoin Exchange Rate")
    try:
        df = client.query_df("SELECT dt, price_usd FROM repeater.btc_exchange_rate")
    except:
        df = None
        st.text("Can't read data for the 'BTC, $' plot. Make sure to run 'bitcoin' job at least once.")
    if df is not None:
        fig = go.Figure()
        fig.add_trace(go.Scatter(x=df['dt'], y=df['price_usd'], line_color='black'))
        fig.update_layout(title='BTC, $',
                          yaxis_range=[0, max(df['price_usd'])*1.1],
                          hovermode="x",
                          height=550)
        st.plotly_chart(fig)
