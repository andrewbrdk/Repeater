import argparse

def main():
    parser = argparse.ArgumentParser(description="Echo scheduled date")
    parser.add_argument('--scheduled_dt', type=str, required=True,
                        help='Scheduled run date in yyyy-mm-dd format')
    args = parser.parse_args()
    print(args.scheduled_dt)

if __name__ == "__main__":
    main()