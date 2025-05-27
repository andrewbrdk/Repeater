#!/usr/bin/env python3
import argparse
import smtplib
from email.message import EmailMessage
import sys
import os
import requests

SMTP_SERVER = os.environ.get("REPEATER_SMTP_SERVER", "")
SMTP_PORT = int(os.environ.get("REPEATER_SMTP_PORT", "587"))
SMTP_USER = os.environ.get("REPEATER_SMTP_USER", "")
SMTP_PASS = os.environ.get("REPEATER_SMTP_PASS", "")
EMAIL_FROM = os.environ.get("REPEATER_EMAIL_FROM", "")
SMTP_TIMEOUT_SECONDS = 10
SLACK_WEBHOOK = os.environ.get("REPEATER_SLACK_WEBHOOK", "")

MSG = """
Task Failed
Job: {job}
Task: {task}
Start: {start}
End: {end}
"""
#todo: add link to task

def send_email(subject, body, recipients):
    m = EmailMessage()
    m['Subject'] = subject
    m['From'] = EMAIL_FROM
    m['To'] = ', '.join(recipients)
    m.set_content(body)
    try:
        with smtplib.SMTP(SMTP_SERVER, SMTP_PORT, timeout=SMTP_TIMEOUT_SECONDS) as server:
            server.starttls()
            server.login(SMTP_USER, SMTP_PASS)
            server.send_message(m)
            print("Email sent successfully.")
    except Exception as e:
        print(f"Error sending email: {e}")

def send_slack(body):
    if not SLACK_WEBHOOK:
        print("No Slack webhook configured, skipping Slack notification.", file=sys.stderr)
        return
    payload = {"text": body}
    try:
        resp = requests.post(SLACK_WEBHOOK, json=payload, timeout=10)
        if resp.status_code >= 300:
            print(f"Slack notification failed: {resp.status_code} {resp.text}", file=sys.stderr)
        else:
            print("Slack notification sent.")
    except Exception as e:
        print(f"Failed to send Slack notification: {e}", file=sys.stderr)

def main():
    parser = argparse.ArgumentParser(description="Send notification on task failure")
    parser.add_argument('--job', required=True, help='Job title')
    parser.add_argument('--task', required=True, help='Task name')
    parser.add_argument('--start', required=True, help='Task start time')
    parser.add_argument('--end', required=True, help='Task end time')
    parser.add_argument('--emails', nargs='+', help='List of recipient email addresses')
    parser.add_argument('--slack', nargs='+', help='List of Slack username mentions (e.g. @user)')
    args = parser.parse_args()

    body = MSG.format(**vars(args))

    if args.emails:
        subject = f"[Repeater] Task Failure: {args.job} / {args.task}"
        send_email(subject, body, args.emails)
    if SLACK_WEBHOOK:
        slack_mentions = " ".join(args.slack) + "\n" if args.slack else ""
        slack_body = slack_mentions + body
        send_slack(slack_body)
    if not args.emails and not SLACK_WEBHOOK:
        print("No email or Slack notifications configured, exiting.")

if __name__ == "__main__":
    main()
