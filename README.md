hydra [![License](http://img.shields.io/:license-gpl3-blue.svg)](http://www.gnu.org/licenses/gpl-3.0.html) [![Build Status](https://travis-ci.org/opennota/hydra.png?branch=master)](https://travis-ci.org/opennota/hydra)
=====

Hydra is a penetration testing tool exclusively focused on dictionary-attacking web-based login forms.

## Installation

    go get -u github.com/opennota/hydra

## Usage

    hydra -L logins.txt -P passwords.txt http://127.0.0.1/login "user=^USER^&pass=^PASS^" "invalid password"

Run `hydra -help` for additional options.
