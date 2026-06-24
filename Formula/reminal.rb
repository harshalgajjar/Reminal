class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.17"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.17/reminal_0.7.17_darwin_arm64.tar.gz"
      sha256 "442d46344db5b8d7034012d5c257c39aa16795a76525785df47f6abee40e991a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.17/reminal_0.7.17_darwin_amd64.tar.gz"
      sha256 "d34dc5ac8e197f34751247b1c228a565d774f708077dc390d2079c737057b11e"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.17/reminal_0.7.17_linux_arm64.tar.gz"
      sha256 "32eaa7f766bc17e716364f0d5d2a63f9c0517aa5386a0e3f2bf567aa44bad78e"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
