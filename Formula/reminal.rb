class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_darwin_arm64.tar.gz"
      sha256 "63fca233f24d9e96256d7213f211196904a774481ee6312b7cfeff32d3ad9438"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_darwin_amd64.tar.gz"
      sha256 "5f1f65d0218ab9ba54789c101798b37bf9603cd716553d2cb86855f6de7beba4"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_linux_arm64.tar.gz"
      sha256 "2622777e0385ce42b9acf1526e5f5f357b81d9b2054815203dc33e12ed179d2b"
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
