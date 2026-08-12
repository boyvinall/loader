#!/bin/bash

# just print something
date

# randomly exit with 0 or 1 after a few seconds
if (( RANDOM % 2 )); then
    sleep 5
    exit 0
else
    sleep 6
    exit 1
fi


