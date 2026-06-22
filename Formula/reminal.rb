class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.4.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.5/reminal_0.4.5_darwin_arm64.tar.gz"
      sha256 "913a69d57c89e5f4c9d340016f493617a8688fb2f9c763d247324f9ab701181c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.5/reminal_0.4.5_darwin_amd64.tar.gz"
      sha256 "11def34f5c25bebc2203dae3b6c3d8080e2b7a33d131e9415fb248d8064958d0"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.5/reminal_0.4.5_linux_arm64.tar.gz"
      sha256 "4d32bdc8d3c4d65406d007c204e19576de241821c0f02fdf54a8472e9af51dcd"
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
