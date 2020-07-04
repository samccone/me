#!/bin/bash

ls **/*.html | xargs -n1 -I{} ./build_tools/main -inline {}
git commit -a -m "build"
git push -f origin main:gh-pages
git reset --hard HEAD~1
