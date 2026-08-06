#!/usr/bin/env python3
"""Generate Debian package fixtures for the manual test playbook.

Creates valid packages and a set of deliberately malformed files under the
output directory (default: demo/fixtures). Uses only the Python standard
library so no dpkg tooling is required.
"""

import gzip
import io
import os
import sys
import tarfile


def ar_member(name, data):
    if len(data) % 2:
        data += b"\n"
    header = "{:<16}{:<12}{:<6}{:<6}{:<8}{:<10}`\n".format(
        name, "0", "0", "0", "100644", str(len(data))
    ).encode("ascii")
    return header + data


def ar_archive(members):
    output = b"!<arch>\n"
    for name, data in members:
        output += ar_member(name, data)
    return output


def tar_gz(files):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for name, data in files.items():
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o644
            info.mtime = 0
            archive.addfile(info, io.BytesIO(data))
    return gzip.compress(buffer.getvalue(), mtime=0)


def deb(control, data_files):
    return ar_archive([
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", tar_gz({"./control": control.encode()})),
        ("data.tar.gz", tar_gz(data_files)),
    ])


def control(name, version, architecture, description, extra=""):
    return (
        f"Package: {name}\n"
        f"Version: {version}\n"
        f"Architecture: {architecture}\n"
        "Maintainer: Deb S3 Demo <demo@example.test>\n"
        "Section: utils\n"
        "Priority: optional\n"
        f"Description: {description}\n"
        f"{extra}"
    )


def valid_packages():
    doc = "./usr/share/doc/demo/README"
    yield "demo_1.0-1_amd64.deb", deb(
        control("demo", "1.0-1", "amd64", "Demo package for deb-s3"),
        {doc: b"demo 1.0-1\n"},
    )
    yield "demo_1.1-1_amd64.deb", deb(
        control("demo", "1.1-1", "amd64", "Demo package for deb-s3"),
        {doc: b"demo 1.1-1\n"},
    )
    # Same name and version as demo_1.0-1 but different contents, for the
    # --fail-if-exists conflict demonstration.
    yield "conflict/demo_1.0-1_amd64.deb", deb(
        control("demo", "1.0-1", "amd64", "Demo package for deb-s3"),
        {doc: b"demo 1.0-1 REBUILT WITH DIFFERENT CONTENTS\n"},
    )
    yield "demo-all_2.0_all.deb", deb(
        control("demo-all", "2.0", "all", "Architecture-independent demo package"),
        {"./usr/share/demo-all/data": b"arch all\n"},
    )
    yield "demo-epoch_2.0-1_amd64.deb", deb(
        control("demo-epoch", "1:2.0-1", "amd64", "Demo package with an epoch"),
        {"./usr/share/demo-epoch/data": b"epoch\n"},
    )
    # Multi-line Description and a folded Tag field: exercises control-field
    # folding through parse -> render round trips.
    yield "demo-tags_1.0-1_amd64.deb", deb(
        control(
            "demo-tags", "1.0-1", "amd64",
            "Demo package with folded fields\n"
            " This description has a continuation line.\n"
            " .\n"
            " And a second paragraph.",
            "Tag: role::program, implemented-in::c,\n"
            " interface::commandline\n",
        ),
        {"./usr/share/demo-tags/data": b"tags\n"},
    )


def malformed_packages():
    good = deb(control("bad", "1.0-1", "amd64", "placeholder"), {"./x": b"x\n"})
    yield "bad-empty.deb", b""
    yield "bad-garbage.deb", os.urandom(1024)
    yield "bad-first-member.deb", ar_archive([("hello.txt", b"hello\n")])
    yield "bad-format-version.deb", ar_archive([
        ("debian-binary", b"3.0\n"),
        ("control.tar.gz", tar_gz({"./control": b"Package: bad\nVersion: 1.0\n"})),
        ("data.tar.gz", tar_gz({"./x": b"x\n"})),
    ])
    yield "bad-missing-data.deb", ar_archive([
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", tar_gz({"./control": b"Package: bad\nVersion: 1.0\n"})),
    ])
    yield "bad-compression.deb", ar_archive([
        ("debian-binary", b"2.0\n"),
        ("control.tar.lzma", b"not really lzma"),
        ("data.tar.gz", tar_gz({"./x": b"x\n"})),
    ])
    yield "bad-no-control-file.deb", ar_archive([
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", tar_gz({"./unrelated": b"nothing here\n"})),
        ("data.tar.gz", tar_gz({"./x": b"x\n"})),
    ])
    yield "bad-truncated.deb", good[: len(good) // 2]


def main():
    directory = sys.argv[1] if len(sys.argv) > 1 else os.path.join("demo", "fixtures")
    for group in (valid_packages, malformed_packages):
        for name, contents in group():
            path = os.path.join(directory, name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "wb") as output:
                output.write(contents)
            print(f"wrote {path} ({len(contents)} bytes)")


if __name__ == "__main__":
    main()
