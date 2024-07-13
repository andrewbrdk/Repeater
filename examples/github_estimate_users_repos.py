import sys
import requests

GH_USERS = "https://api.github.com/users"
GH_REPOS = "https://api.github.com/repositories"
MIN_INITIAL_POW = 25
MAX_INITIAL_POW = 40

def find_initial(url):
    max_nonempty_power = None
    for i in range(MIN_INITIAL_POW, MAX_INITIAL_POW):
        testid = 2**i
        r = requests.get(url, params={"since": testid})
        if r and len(r.json()) == 0:
            max_nonempty_power = i - 1
            break
        elif r:
            pass
        elif not r and r.status_code == 403:
            print(r.status_code)
            print(r.json().get('message', ''))
            sys.exit(1)
        else:
            print(r.status_code)
            sys.exit(1)
    return max_nonempty_power

def estimate_thousands(url, max_nonempty_power):
    testid = 2**max_nonempty_power
    for i in range(max_nonempty_power-1, 9, -1):
        tmptestid = testid + 2**i
        r = requests.get(url, params={"since": tmptestid})
        if r and len(r.json()) != 0:
            testid = tmptestid
        elif r:
            pass
        elif not r and r.status_code == 403:
            print(r.status_code)
            print(r.json().get('message', ''))
            sys.exit(1)
        else:
            print(r.status_code)
            sys.exit(1)
    return testid

def main():
    i = find_initial(GH_USERS)
    est_max_userid = estimate_thousands(GH_USERS, i)
    print(f"Estimated number of users: {est_max_userid}")
    i = find_initial(GH_REPOS)
    est_max_repoid = estimate_thousands(GH_REPOS, i)
    print(f"Estimated number of repos: {est_max_repoid}")

if __name__ == "__main__":
    main()
