class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.15"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.15/reminal_0.7.15_darwin_arm64.tar.gz"
      sha256 "ebcf6d626e4ec7a732e263622a115699667a8acc17c609740ff541dc29c5b353"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.15/reminal_0.7.15_darwin_amd64.tar.gz"
      sha256 "81bea53dafb3c973886fc16be481e689336042aef157a3fc2905b2f0e94a87fe"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.15/reminal_0.7.15_linux_arm64.tar.gz"
      sha256 "15432d86c578bdbf3637a166c9dacffb88204b951c2e14ae31a4b3c421c2cf0b"
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
