# Formula template for the ivanrvpereira/homebrew-tap repository.
# The release workflow fills in the version and sha256 placeholders, checks the result with brew style and
# brew audit, attaches it to the GitHub release, and pushes it to Formula/agentmailguard.rb in the tap.
# Edit this file, not the copy in the tap.
class Agentmailguard < Formula
  desc "Screens Microsoft 365 and Gmail mail and calendars for AI agents"
  homepage "https://github.com/ivanrvpereira/agentmailguard"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/ivanrvpereira/agentmailguard/releases/download/v{{VERSION}}/agentmailguard_{{VERSION}}_darwin_arm64.tar.gz"
      sha256 "{{SHA256_DARWIN_ARM64}}"
    end
    on_intel do
      url "https://github.com/ivanrvpereira/agentmailguard/releases/download/v{{VERSION}}/agentmailguard_{{VERSION}}_darwin_amd64.tar.gz"
      sha256 "{{SHA256_DARWIN_AMD64}}"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/ivanrvpereira/agentmailguard/releases/download/v{{VERSION}}/agentmailguard_{{VERSION}}_linux_arm64.tar.gz"
      sha256 "{{SHA256_LINUX_ARM64}}"
    end
    on_intel do
      url "https://github.com/ivanrvpereira/agentmailguard/releases/download/v{{VERSION}}/agentmailguard_{{VERSION}}_linux_amd64.tar.gz"
      sha256 "{{SHA256_LINUX_AMD64}}"
    end
  end

  def install
    bin.install "amg", "amgd"
  end

  def caveats
    <<~EOS
      amgd runs the daemon and amg is the agent's client. Set up providers, accounts,
      and screening before the first `amgd serve`:
        https://github.com/ivanrvpereira/agentmailguard/blob/main/docs/setup.md
    EOS
  end

  test do
    assert_equal "amg version v#{version}", shell_output("#{bin}/amg --version").strip
    assert_equal "amgd version v#{version}", shell_output("#{bin}/amgd --version").strip
  end
end
