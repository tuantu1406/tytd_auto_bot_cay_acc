@echo off
chcp 65001 >nul
title TYTD Auto Grind Tool - Cay Acc Lv 100
echo Dang khoi dong TYTD Auto Grind Tool...
go run ./cmd/grind_cli -file accounts.csv
pause
