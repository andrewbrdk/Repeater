#!/usr/bin/env python3
import argparse
import smtplib
from email.message import EmailMessage
import sys
import os

SMTP_SERVER = os.environ.get("NOTIFY_SMTP_SERVER", "localhost")
SMTP_PORT = int(os.environ.get("NOTIFY_SMTP_PORT", "25"))
SMTP_USER = os.environ.get("NOTIFY_SMTP_USER", "")
SMTP_PASS = os.environ.get("NOTIFY_SMTP_PASS", "")
EMAIL_FROM = os.environ.get("NOTIFY_EMAIL_FROM", "repeater@example.com")
EMAIL_TO = os.environ.get("NOTIFY_EMAIL_TO", "admin@example.com")

def main():
    parser = argparse.ArgumentParser(description="Send notification on task failure")
    parser.add_argument('--job', required=True, help='Job title')
    parser.add_argument('--task', required=True, help='Task name')
    parser.add_argument('--status', required=True, help='Task status')
    parser.add_argument('--start', required=True, help='Task start time')
    parser.add_argument('--end', required=True, help='Task end time')
    parser.add_argument('--emails', required=True, nargs='+', help='List of recipient email addresses')
    args = parser.parse_args()

    subject = f"[Repeater] Task Failure: {args.job} / {args.task}"
    body = (
        f"Job: {args.job}\n"
        f"Task: {args.task}\n"
        f"Status: {args.status}\n"
        f"Start: {args.start}\n"
        f"End: {args.end}\n"
    )

    msg = EmailMessage()
    msg['Subject'] = subject
    msg['From'] = EMAIL_FROM
    msg['To'] = ', '.join(args.emails)
    msg.set_content(body)

    try:
        with smtplib.SMTP(SMTP_SERVER, SMTP_PORT) as server:
            if SMTP_USER and SMTP_PASS:
                server.starttls()
                server.login(SMTP_USER, SMTP_PASS)
            server.send_message(msg)
        print("Notification email sent.")
    except Exception as e:
        print(f"Failed to send notification email: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()