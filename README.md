hydra [![License](http://img.shields.io/:license-gpl3-blue.svg)](http://www.gnu.org/licenses/gpl-3.0.html)
=====

Hydra is a penetration testing tool exclusively focused on dictionary-attacking web-based login forms.

## Installation

    go get github.com/opennota/hydra

## Usage

    hydra -L logins.txt -P passwords.txt http://127.0.0.1/login "user=^USER^&pass=^PASS^" "login failed"

Run `hydra --help` for additional options.
