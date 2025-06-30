import os
import sys
import argparse
from datetime import datetime, timedelta

def main():
    parser = argparse.ArgumentParser(description="Delete log files older than keep_days")
    parser.add_argument("--logs_dir", required=True, help="Path to the logs directory")
    parser.add_argument("--keep_days", type=int, required=True, help="Delete files older than this many days")
    args = parser.parse_args()

    if not os.path.isdir(args.logs_dir):
        print(f"Error: Directory '{args.logs_dir}' does not exist.")
        sys.exit(1)
    if args.keep_days < 0:
        print("keep_days must be a non-negative integer.")
        sys.exit(2)

    deleted = 0
    cutoff = datetime.now() - timedelta(days=args.keep_days)
    for fname in os.listdir(args.logs_dir):
        if not fname.endswith('.log'):
            continue
        try:
            file_dt = datetime.strptime(fname[:8], "%Y%m%d")
        except Exception as e:
            print(f"Skipping {fname}: {e}")
            continue
        if file_dt < cutoff:
            fpath = os.path.join(args.logs_dir, fname)
            try:
                os.remove(fpath)
                print(f"Deleted: {fpath}")
                deleted += 1
            except Exception as e:
                print(f"Failed to delete {fpath}: {e}")

    print(f"Done. Deleted {deleted} files older than {args.keep_days} days in {args.logs_dir}.")

if __name__ == "__main__":
    main()