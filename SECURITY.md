# Security

Report a vulnerability privately to security@cernly.eu, not in a public issue. You get an answer within three
working days. Please include the version (`cernly` prints it in every scan as `extractor.version`) and a minimal
repository that shows the problem.

The scanner reads untrusted repositories. Anything that makes it execute repository content, read outside the
git-tracked files, follow a symlink or submodule, or leak a value from an env file is a vulnerability. That
includes the `.git` folder: a repository handed over with its `.git` (an archive, a shared CI workspace) must not
make git run a program its config names.
