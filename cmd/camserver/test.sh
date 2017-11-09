#!/bin/sh

echo '
ls camserver@example.com
info camserver@example.com/frame.jpg
cp camserver@example.com/frame.jpg .
' | upbox -schema=camserver.upbox
