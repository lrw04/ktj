import json
import zipfile
from pathlib import Path
from sys import argv

def main(f):
    p = []
    with zipfile.ZipFile(f) as z:
        l = list(filter(lambda x: x.startswith("tests/"), z.namelist()))
        for f in l:
            if f + ".a" not in l:
                continue
            with z.open(f) as inp, z.open(f + ".a") as ans:
                p.append({"input": inp.read().decode(), "answer": ans.read().decode()})
    print(json.dumps(p))

if __name__ == "__main__":
    main(Path(argv[1]))
