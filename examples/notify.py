#!/usr/bin/env python3
import os
import sys
import requests
import argparse
import smtplib
from email.message import EmailMessage

from connections import SMTP, SLACK

MSG = """
Task Failed
Job: {job}
Task: {task}
Start: {start}
End: {end}
"""
#todo: add link to task

def send_email(subject, body, recipients):
    if not SMTP.get('server'):
        print("No SMTP server configured, skipping email notification.", file=sys.stderr)
        return
    m = EmailMessage()
    m['Subject'] = subject
    m['From'] = SMTP.get('email_from')
    m['To'] = ', '.join(recipients)
    m.set_content(body)
    try:
        with smtplib.SMTP(SMTP.get('server'), SMTP.get('port'), timeout=SMTP.get('timeout')) as server:
            server.starttls()
            server.login(SMPT.get('username'), SMTP.get('password'))
            server.send_message(m)
            print("Email sent successfully.")
    except Exception as e:
        print(f"Error sending email: {e}")

def send_slack(body):
    if not SLACK.get('webhook'):
        print("No Slack webhook configured, skipping Slack notification.", file=sys.stderr)
        return
    payload = {"text": body}
    try:
        resp = requests.post(SLACK.get('webhook'), json=payload, timeout=10)
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
    args = parser.parse_args()

    body = MSG.format(**vars(args))

    if args.emails:
        subject = f"[Repeater] Task Failure: {args.job} / {args.task}"
        send_email(subject, body, args.emails)
    if SLACK_WEBHOOK:
        send_slack(body)
    if not args.emails and not SLACK_WEBHOOK:
        print("No email or Slack notifications configured, exiting.")

if __name__ == "__main__":
    main()
