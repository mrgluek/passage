#!/usr/bin/env python3
"""Writes the Homebrew cask and formula for one release into a tap checkout.

Usage: homebrew-tap.py <version> <SHA256SUMS> <tap-dir>
"""
import os
import sys

version, sums_path, tap = sys.argv[1:4]
sums = {}
with open(sums_path, encoding="utf-8") as f:
    for line in f:
        digest, _, name = line.strip().partition("  ")
        sums[name] = digest


def sha(name):
    if name not in sums:
        sys.exit(f"{name} is not in {sums_path}")
    return sums[name]


base = f"https://github.com/mrgluek/passage/releases/download/v{version}"
cask = f'''cask "passage" do
  arch arm: "arm64", intel: "amd64"

  version "{version}"
  sha256 arm:   "{sha(f"passage-{version}-darwin-arm64.zip")}",
         intel: "{sha(f"passage-{version}-darwin-amd64.zip")}"

  url "https://github.com/mrgluek/passage/releases/download/v#{{version}}/passage-#{{version}}-darwin-#{{arch}}.zip"
  name "Passage"
  desc "Local SOCKS5 proxy for messaging apps"
  homepage "https://passage.gluek.info"

  auto_updates true
  depends_on macos: ">= :ventura"

  app "Passage.app"

  # The app is signed ad hoc, not notarized: remove the download quarantine.
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{{appdir}}/Passage.app"],
                   must_succeed: false
  end

  zap trash: "~/Library/Application Support/Passage"
end
'''


def block(os_name, arch_block, arch):
    name = f"passage-cli-{version}-{os_name}-{arch}.tar.gz"
    return f'''    {arch_block} do
      url "{base}/{name}"
      sha256 "{sha(name)}"
    end
'''


formula = f'''class PassageCli < Formula
  desc "Local SOCKS5 proxy for messaging apps (command-line)"
  homepage "https://passage.gluek.info"
  license "MIT"

  on_macos do
{block("darwin", "on_arm", "arm64")}{block("darwin", "on_intel", "amd64")}  end

  on_linux do
{block("linux", "on_arm", "arm64")}{block("linux", "on_intel", "amd64")}  end

  def install
    # passage-iroh must stay next to passage-cli, which finds it by its real path.
    libexec.install "passage-cli", "passage-iroh"
    bin.install_symlink libexec/"passage-cli"
  end

  test do
    assert_predicate libexec/"passage-iroh", :executable?
    assert_match "-link-file", shell_output("#{{bin}}/passage-cli -h 2>&1")
  end
end
'''
for rel, text in (("Casks/passage.rb", cask), ("Formula/passage-cli.rb", formula)):
    path = os.path.join(tap, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
