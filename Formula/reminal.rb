class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.5/reminal_0.7.5_darwin_arm64.tar.gz"
      sha256 "41680c82a5e8392c5f69b8038f232b0fa19e9b5025903878f860cc96d10ce129"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.5/reminal_0.7.5_darwin_amd64.tar.gz"
      sha256 "60ac968a09b72ba9a475716c9279322d2f269a7c7a3a03197b7377c3e5177031"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.5/reminal_0.7.5_linux_arm64.tar.gz"
      sha256 "5200c56678756c41fe8ec7175826a3d84c72dea6ce44676b2f3f2473c8d1bcd8"
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
