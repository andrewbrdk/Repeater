import argparse

def main():
    parser = argparse.ArgumentParser(description="Echo templated arguments")
    parser.add_argument('--title', type=str, required=True,
                        help='Job title')
    parser.add_argument('--scheduled_dt', type=str, required=True,
                        help='Scheduled run date in yyyy-mm-dd format')
    args = parser.parse_args()
    print('{{.title}}:', args.title)
    print('{{.scheduled_dt}}:', args.scheduled_dt)

if __name__ == "__main__":
    main()