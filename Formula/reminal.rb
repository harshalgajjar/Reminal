class Reminal < Formula
  desc "Remote terminal access from any browser — no SSH, no port forwarding"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.1.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.2/reminal_0.1.2_darwin_arm64.tar.gz"
      sha256 "d53792e2d00338e70571c2136b88efa35e05b8d342d50ccc9c2c3e504687f74b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.2/reminal_0.1.2_darwin_amd64.tar.gz"
      sha256 "8c86afd4be688f5e1689cf0f8397511a79bbacea19a2dc7c43eb69ab8b399f9a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.2/reminal_0.1.2_linux_arm64.tar.gz"
      sha256 "f55a819003a437e45392974c44a180681d79190c88ef1afa93694744be37feb0"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
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
