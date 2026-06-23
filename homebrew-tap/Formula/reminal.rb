class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.6"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.6/reminal_0.7.6_darwin_arm64.tar.gz"
      sha256 "b3b90dfc1f21812a635dfcdd369ca568754edeb558ae23aa4fb2bce02c92a922"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.6/reminal_0.7.6_darwin_amd64.tar.gz"
      sha256 "febd6d7a9a913bc13571bbda81766001683644d05cb61c764701fc17ddb13765"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.6/reminal_0.7.6_linux_arm64.tar.gz"
      sha256 "a974d9467a41905db0ef7b96ab120600fd5e6dc48785dc665ba543502cccf5ba"
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
