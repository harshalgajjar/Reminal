class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.4.3"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.3/reminal_1.4.3_darwin_arm64.tar.gz"
      sha256 "102e69bd3d70f372859cbafaff252ca9a7ab6d33bf4838d2c980a20dd5b364da"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.3/reminal_1.4.3_darwin_amd64.tar.gz"
      sha256 "870182118c79523d3900d0a526d216cf51612f5f37e699a9055407a8694f00a7"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.3/reminal_1.4.3_linux_arm64.tar.gz"
      sha256 "8fbf68e4a6e2c6b169938ba19a85fd15946854c6adc728d85287cc6b044b5dee"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.3/reminal_1.4.3_linux_amd64.tar.gz"
      sha256 "b2a654794cea243d43b32371074f1f7b7b6de09f5e351991ffa1007beec25c91"
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
