from pathlib import Path
from sys import argv
import json

def main(path: Path):
    tests = []
    for f in path.glob("*.in"):
        ans = f.with_suffix(".ans")
        tests.append({"input": f.read_text(), "answer": ans.read_text()})
    print(json.dumps(tests))

if __name__ == "__main__":
    main(Path(argv[1]))
